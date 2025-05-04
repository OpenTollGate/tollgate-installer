import React, { useState, useEffect } from 'react';
import styled from 'styled-components';
import Card from './common/Card';
import ProgressBar from './common/ProgressBar';

interface RouterInfo {
  ip: string;
  boardName?: string;
  architecture?: string;
  compatible?: boolean;
}

interface InstallerProps {
  router: RouterInfo | null;
  progress: number;
  error: string | null;
}

const InstallerContainer = styled.div`
  padding: 1rem 0;
`;

const StatusSection = styled.div`
  margin-bottom: 2rem;
`;

const StatusTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.text};
  margin-bottom: 0.5rem;
`;

const StatusMessage = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: 1.5rem;
`;

const ErrorMessage = styled.div`
  color: ${props => props.theme.colors.error};
  background-color: ${props => props.theme.colors.primaryLight};
  padding: 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1rem;
`;

const RouterDetails = styled.div`
  background-color: ${props => props.theme.colors.backgroundAlt};
  border-radius: ${props => props.theme.radii.md};
  padding: 1rem;
  margin-bottom: 1.5rem;
`;

const RouterDetail = styled.div`
  display: flex;
  justify-content: space-between;
  margin-bottom: 0.5rem;
  
  &:last-child {
    margin-bottom: 0;
  }
`;

const DetailLabel = styled.span`
  font-weight: ${props => props.theme.fontWeights.medium};
  color: ${props => props.theme.colors.text};
`;

const DetailValue = styled.span`
  color: ${props => props.theme.colors.textSecondary};
`;

const StepList = styled.div`
  margin-top: 1.5rem;
`;

const Step = styled.div<{ $active: boolean; $completed: boolean }>`
  display: flex;
  align-items: center;
  margin-bottom: 1rem;
  opacity: ${props => (props.$active || props.$completed ? 1 : 0.5)};
  
  &:last-child {
    margin-bottom: 0;
  }
`;

const StepIcon = styled.div<{ $completed: boolean; $active: boolean }>`
  width: 24px;
  height: 24px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  margin-right: 1rem;
  background-color: ${props => {
    if (props.$completed) return props.theme.colors.success;
    if (props.$active) return props.theme.colors.primary;
    return props.theme.colors.border;
  }};
  color: white;
  font-size: 12px;
  font-weight: bold;
`;

const StepText = styled.div`
  flex: 1;
`;

const StepTitle = styled.div`
  font-weight: ${props => props.theme.fontWeights.medium};
  color: ${props => props.theme.colors.text};
`;

const StepDescription = styled.div`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
`;

const Installer: React.FC<InstallerProps> = ({
  router,
  progress: initialProgress,
  error
}) => {
  const [progress, setProgress] = useState(0);
  const [currentStep, setCurrentStep] = useState(0);
  
  const steps = [
    { 
      title: 'Preparing Installation', 
      description: 'Checking router compatibility and preparing files' 
    },
    { 
      title: 'Downloading TollGateOS Image', 
      description: 'Retrieving the firmware image optimized for your router' 
    },
    { 
      title: 'Transferring Files to Router', 
      description: 'Uploading the firmware image to your router' 
    },
    { 
      title: 'Installing TollGateOS', 
      description: 'Flashing the firmware and configuring your router' 
    },
    { 
      title: 'Finalizing Setup', 
      description: 'Completing the installation and starting services' 
    }
  ];
  
  // Simulate installation progress
  useEffect(() => {
    if (initialProgress > 0) {
      setProgress(initialProgress);
      return;
    }
    
    const interval = setInterval(() => {
      setProgress(prev => {
        if (prev >= 100) {
          clearInterval(interval);
          return 100;
        }
        
        // Update current step based on progress
        const newStep = Math.floor(prev / 20);
        if (newStep !== currentStep) {
          setCurrentStep(newStep);
        }
        
        return prev + 0.5;
      });
    }, 100);
    
    return () => clearInterval(interval);
  }, [initialProgress, currentStep]);
  
  return (
    <Card
      title="Installing TollGateOS"
      subtitle={`Installing on router ${router?.ip || ''}`}
    >
      {error && <ErrorMessage>{error}</ErrorMessage>}
      
      <InstallerContainer>
        <StatusSection>
          <StatusTitle>
            {progress < 100 ? 'Installation in progress...' : 'Installation complete!'}
          </StatusTitle>
          <StatusMessage>
            {progress < 100 
              ? 'Please wait while TollGateOS is being installed on your router. This may take a few minutes.' 
              : 'TollGateOS has been successfully installed on your router!'}
          </StatusMessage>
          
          <ProgressBar 
            progress={progress} 
            color={progress >= 100 ? 'success' : 'primary'} 
          />
        </StatusSection>
        
        {router && (
          <RouterDetails>
            <RouterDetail>
              <DetailLabel>Router IP:</DetailLabel>
              <DetailValue>{router.ip}</DetailValue>
            </RouterDetail>
            {router.boardName && (
              <RouterDetail>
                <DetailLabel>Model:</DetailLabel>
                <DetailValue>{router.boardName}</DetailValue>
              </RouterDetail>
            )}
            {router.architecture && (
              <RouterDetail>
                <DetailLabel>Architecture:</DetailLabel>
                <DetailValue>{router.architecture}</DetailValue>
              </RouterDetail>
            )}
          </RouterDetails>
        )}
        
        <StepList>
          {steps.map((step, index) => (
            <Step
              key={index}
              $active={index === currentStep}
              $completed={index < currentStep || (index === steps.length - 1 && progress >= 100)}
            >
              <StepIcon
                $completed={index < currentStep || (index === steps.length - 1 && progress >= 100)}
                $active={index === currentStep}
              >
                {index < currentStep || (index === steps.length - 1 && progress >= 100) ? '✓' : index + 1}
              </StepIcon>
              <StepText>
                <StepTitle>{step.title}</StepTitle>
                <StepDescription>{step.description}</StepDescription>
              </StepText>
            </Step>
          ))}
        </StepList>
      </InstallerContainer>
    </Card>
  );
};

export default Installer;